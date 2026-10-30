package statistic

import (
	"encoding/json"
	"net/http"

	"imuslab.com/zoraxy/mod/utils"
)

func (c *Collector) HandleTodayStatLoad(w http.ResponseWriter, r *http.Request) {

	fast, _ := utils.GetBool(r, "fast")
	if fast {
		//Only return the counter
		total, errs, valid := c.Summary().Counts()
		exported := DailySummaryExport{
			TotalRequest: total,
			ErrorRequest: errs,
			ValidRequest: valid,
		}
		js, _ := json.Marshal(exported)
		utils.SendJSONResponse(w, string(js))
	} else {
		//Return everything
		exported := c.GetExportSummary()
		js, _ := json.Marshal(exported)
		utils.SendJSONResponse(w, string(js))
	}

}
