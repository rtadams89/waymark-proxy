package statistic

import (
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"imuslab.com/zoraxy/mod/database"
)

// Collects request statistics and stores them for analysis

// DailySummary is an interval summary of all collected data
type DailySummary struct {
	TotalRequest int64 //Total request of the day
	ErrorRequest int64 //Invalid request of the day, including error or not found
	ValidRequest int64 //Valid request of the day
	//Type counters
	ForwardTypes        *sync.Map //Map that hold the forward types
	RequestOrigin       *sync.Map //Map that hold [country ISO code]: visitor counter
	RequestClientIp     *sync.Map //Map that hold all unique request IPs
	Referer             *sync.Map //Map that store where the user was refered from
	UserAgent           *sync.Map //Map that store the useragent of the request
	RequestURL          *sync.Map //Request URL of the request object
	RequestMethods      *sync.Map //Map that hold the HTTP methods (GET / POST / ...) of the requests
	DownstreamHostnames *sync.Map //Request count of downstream hostname
	UpstreamHostnames   *sync.Map //Forwarded request count of upstream hostname

	// bounded tracks the logical size of each map above and serializes the trim path
	bounded boundedCounters
}

type RequestInfo struct {
	IpAddr                        string //IP address of the downstream request
	RequestOriginalCountryISOCode string //ISO code of the country where the request originated
	Succ                          bool   // If the request is successful and the response came from the upstream
	StatusCode                    int    //HTTP status code of the request
	ForwardType                   string //Forward type of the request, usually the proxy type (e.g. host-http, subdomain-websocket or vdir-http or any of the combination)
	RequestMethod                 string //HTTP method of the downstream request (e.g. GET, POST)
	Referer                       string //Referer of the downstream request
	UserAgent                     string //UserAgent of the downstream request
	RequestURL                    string //Request URL
	Target                        string //Target domain or hostname
	Upstream                      string // Upstream domain or hostname, if forwarded
}

type CollectorOption struct {
	Database *database.Database
	// MaxEntriesPerStatMap is the per-dimension soft cap for the sync.Map fields
	// in DailySummary; 0 means unbounded. Past the cap the entries with the
	// lowest request counts are dropped (bounded.go).
	MaxEntriesPerStatMap int
}

type Collector struct {
	rtdataStopChan chan bool
	autoSaveTicker *time.Ticker
	autSaveStop    chan bool
	summary        atomic.Pointer[DailySummary] // Today's summary; swapped at midnight
	Option         *CollectorOption
	summaryKey     string     //Date key (2006_01_02) that DailySummary belongs to
	summaryMu      sync.Mutex //Guards DailySummary swaps together with summaryKey
	// incr is the per-request map-increment strategy, chosen once at startup
	// based on Option.MaxEntriesPerStatMap. See newIncrFn.
	incr incrFn
}

// incrFn increments one entry in one of the per-dimension sync.Maps: either
// unboundedIncr or a closure over boundedIncr.
type incrFn func(m *sync.Map, b *boundedCounter, key string)

func newIncrFn(maxEntries int) incrFn {
	if maxEntries <= 0 {
		return unboundedIncr
	}
	capN := maxEntries
	return func(m *sync.Map, b *boundedCounter, key string) {
		boundedIncr(m, b, key, capN)
	}
}

// unboundedIncr increments key in m without a cap (MaxEntriesPerStatMap == 0).
func unboundedIncr(m *sync.Map, _ *boundedCounter, key string) {
	incrKey(m, key)
}

// incrKey adds one to key's count, inserting it with 1 if absent, using
// compare-and-swap. Returns true if the key was new.
func incrKey(m *sync.Map, key string) bool {
	for {
		v, loaded := m.LoadOrStore(key, 1)
		if !loaded {
			return true
		}
		if m.CompareAndSwap(key, v, v.(int)+1) {
			return false
		}
	}
}

func NewStatisticCollector(option CollectorOption) (*Collector, error) {
	option.Database.NewTable("stats")

	//Create the collector object
	thisCollector := Collector{
		Option:     &option,
		incr:       newIncrFn(option.MaxEntriesPerStatMap),
		summaryKey: summaryKeyOf(time.Now()),
	}

	// Load today's stat if it exists
	year, month, day := time.Now().Date()
	summary := thisCollector.LoadSummaryOfDay(year, month, day)
	if summary == nil {
		summary = NewDailySummary()
	}
	thisCollector.summary.Store(summary)

	//Schedule the realtime statistic clearing at midnight everyday
	rtstatStopChan := thisCollector.ScheduleResetRealtimeStats()
	thisCollector.rtdataStopChan = rtstatStopChan

	return &thisCollector, nil
}

// Set the autosave duration, the collector will save the daily summary to database
// set saveInterval to 0 to disable autosave
func (c *Collector) SetAutoSave(saveInterval int) {
	//Stop the current ticker if exists
	if c.autSaveStop != nil {
		c.autSaveStop <- true
	}

	if saveInterval == 0 {
		return
	}

	c.autSaveStop = make(chan bool)
	ticker := time.NewTicker(time.Duration(saveInterval) * time.Second)
	c.autoSaveTicker = ticker

	go func() {
		for {
			select {
			case <-ticker.C:
				c.SaveSummaryOfDay()
			case <-c.autSaveStop:
				ticker.Stop()
				return
			}
		}
	}()
}

// summaryKeyOf returns the database key of the daily summary for the given time
func summaryKeyOf(t time.Time) string {
	return t.Format("2006_01_02")
}

// Write the current summary to database, rolling over to a new day if the date changed
func (c *Collector) SaveSummaryOfDay() {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()

	todayKey := summaryKeyOf(time.Now())
	if c.summaryKey != todayKey {
		//Day rolled over, persist the finished day and start a new summary
		c.writeSummary(c.summaryKey, c.Summary())
		c.summary.Store(NewDailySummary())
		c.summaryKey = todayKey
	}

	c.writeSummary(c.summaryKey, c.Summary())
}

// writeSummary writes the given summary to the database under summaryKey
func (c *Collector) writeSummary(summaryKey string, summary *DailySummary) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("[Statistic] Recovered from panic while saving daily summary: ", r)
		}
	}()

	saveData := DailySummaryToExport(summary)
	err := c.Option.Database.Write("stats", summaryKey, saveData)
	if err != nil {
		log.Println("[Statistic] Failed to save daily summary of "+summaryKey+": ", err)
	}
}

// Get the daily summary up until now
func (c *Collector) GetCurrentDailySummary() *DailySummary {
	return c.Summary()
}

// Summary returns today's summary. Its counters must be read with
// atomic.LoadInt64 (or Counts), since requests update them concurrently.
func (c *Collector) Summary() *DailySummary {
	return c.summary.Load()
}

// Counts returns the request counters.
func (d *DailySummary) Counts() (total, errs, valid int64) {
	return atomic.LoadInt64(&d.TotalRequest), atomic.LoadInt64(&d.ErrorRequest), atomic.LoadInt64(&d.ValidRequest)
}

// Load the summary of a day given
func (c *Collector) LoadSummaryOfDay(year int, month time.Month, day int) *DailySummary {
	defer func() {
		if r := recover(); r != nil {
			log.Println("[Statistic] Recovered from panic while loading daily summary: ", r)
		}
	}()

	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
	summaryKey := date.Format("2006_01_02")
	targetSummaryExport := DailySummaryExport{}
	c.Option.Database.Read("stats", summaryKey, &targetSummaryExport)
	targetSummary := DailySummaryExportToSummary(targetSummaryExport)
	return &targetSummary
}

// Reset today summary, for debug or restoring injections
func (c *Collector) ResetSummaryOfDay() {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()
	c.summary.Store(NewDailySummary())
	c.summaryKey = summaryKeyOf(time.Now())
}

// This function gives the current slot in the 288- 5 minutes interval of the day
func (c *Collector) GetCurrentRealtimeStatIntervalId() int {
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).Unix()
	secondsSinceStartOfDay := now.Unix() - startOfDay
	interval := secondsSinceStartOfDay / (5 * 60)
	return int(interval)
}

func (c *Collector) Close() {
	//Stop the ticker
	c.rtdataStopChan <- true

	//Write the buffered data into database
	c.SaveSummaryOfDay()
}

// RecordRequest counts one proxied request synchronously: counters are
// updated atomically and the maps with compare-and-swap.
func (c *Collector) RecordRequest(ri RequestInfo) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("[Statistic] Recovered from panic while recording request: ", r)
		}
	}()
	d := c.Summary()

	atomic.AddInt64(&d.TotalRequest, 1)
	if ri.Succ {
		atomic.AddInt64(&d.ValidRequest, 1)
	} else {
		atomic.AddInt64(&d.ErrorRequest, 1)
	}

	// Store the request info into the maps (c.incr is unbounded or capped)
	c.incr(d.ForwardTypes, d.bounded.ForwardTypes, ri.ForwardType)

	//Record the HTTP method of the request
	if ri.RequestMethod != "" {
		c.incr(d.RequestMethods, d.bounded.RequestMethods, ri.RequestMethod)
	}

	originISO := strings.ToLower(ri.RequestOriginalCountryISOCode)
	c.incr(d.RequestOrigin, d.bounded.RequestOrigin, originISO)

	//Filter out CF forwarded requests
	if strings.Contains(ri.IpAddr, ",") {
		ips := strings.Split(strings.TrimSpace(ri.IpAddr), ",")
		if len(ips) >= 1 && IsValidIPAddress(strings.TrimPrefix(ips[0], "[")) {
			//Example when forwarded from CF: 158.250.160.114,109.21.249.211
			//Or IPv6 [15c4:cbb4:cc98:4291:ffc1:3a46:06a1:51a7],109.21.249.211
			ri.IpAddr = ips[0]
		}
	}

	c.incr(d.RequestClientIp, d.bounded.RequestClientIp, ri.IpAddr)

	//Record the referer, without any HTML in it
	if ri.Referer != "" {
		c.incr(d.Referer, d.bounded.Referer, truncateStatKey(refererPolicy.Sanitize(ri.Referer)))
	} else {
		c.incr(d.Referer, d.bounded.Referer, "")
	}

	//Record the UserAgent
	c.incr(d.UserAgent, d.bounded.UserAgent, truncateStatKey(ri.UserAgent))

	//Record request URL, if it is a page
	ext := filepath.Ext(ri.RequestURL)

	if ext != "" && !isWebPageExtension(ext) {
		return
	}

	c.incr(d.RequestURL, d.bounded.RequestURL, truncateStatKey(ri.RequestURL))

	// Record the downstream hostname (the hostname the user visited)
	c.incr(d.DownstreamHostnames, d.bounded.DownstreamHostnames, ri.Target)

	// Record the upstream hostname (the selected load balancer upstream)
	c.incr(d.UpstreamHostnames, d.bounded.UpstreamHostnames, ri.Upstream)
}

// refererPolicy strips HTML from referers (built once, safe for concurrent use).
var refererPolicy = bluemonday.StripTagsPolicy()

// ScheduleResetRealtimeStats saves and rolls over the summary every midnight
func (c *Collector) ScheduleResetRealtimeStats() chan bool {
	doneCh := make(chan bool)

	go func() {
		defer close(doneCh)

		for {
			// calculate duration until next midnight
			now := time.Now()

			// Get midnight of the next day in the local time zone
			midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())

			// Calculate the duration until midnight
			duration := midnight.Sub(now)
			select {
			case <-time.After(duration):
				// save and roll over, an early fire retries on the next loop
				c.SaveSummaryOfDay()
			case <-doneCh:
				// stop the routine
				return
			}
		}
	}()

	return doneCh
}

func NewDailySummary() *DailySummary {
	return &DailySummary{
		TotalRequest:        0,
		ErrorRequest:        0,
		ValidRequest:        0,
		ForwardTypes:        &sync.Map{},
		RequestOrigin:       &sync.Map{},
		RequestClientIp:     &sync.Map{},
		Referer:             &sync.Map{},
		UserAgent:           &sync.Map{},
		RequestURL:          &sync.Map{},
		RequestMethods:      &sync.Map{},
		DownstreamHostnames: &sync.Map{},
		UpstreamHostnames:   &sync.Map{},
		bounded:             newBoundedCounters(),
	}
}
