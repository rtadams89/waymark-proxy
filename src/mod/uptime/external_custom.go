package uptime

// Lets another module supply uptime records for targets marked
// ExternallyMonitored.

// RecordExternal appends a record to a target's history, trimming it to
// MaxRecordsStore like records from this monitor's own checks.
func (m *Monitor) RecordExternal(record Record) {
	m.logMutex.Lock()
	defer m.logMutex.Unlock()
	records := append(m.OnlineStatusLog[record.ID], &record)
	if m.Config.MaxRecordsStore > 0 && len(records) > m.Config.MaxRecordsStore {
		records = records[len(records)-m.Config.MaxRecordsStore:]
	}
	m.OnlineStatusLog[record.ID] = records
}

// IntervalSeconds returns how often this monitor records a target, which is
// also the spacing the Uptime Monitor page expects between records.
func (m *Monitor) IntervalSeconds() int {
	if m.Config.Interval <= 0 {
		return 300
	}
	return m.Config.Interval
}
