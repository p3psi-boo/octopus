package handlers

import (
	"net/http"
	"time"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-gonic/gin"
)

// StatsDailyResponse is the API response format with string date for frontend compatibility
type StatsDailyResponse struct {
	Date           string  `json:"date"`
	InputToken     int64   `json:"input_token"`
	OutputToken    int64   `json:"output_token"`
	InputCost      float64 `json:"input_cost"`
	OutputCost     float64 `json:"output_cost"`
	WaitTime       int64   `json:"wait_time"`
	RequestSuccess int64   `json:"request_success"`
	RequestFailed  int64   `json:"request_failed"`
}

// StatsHourlyResponse is the API response format with string date for frontend compatibility
type StatsHourlyResponse struct {
	Hour           int     `json:"hour"`
	Date           string  `json:"date"`
	InputToken     int64   `json:"input_token"`
	OutputToken    int64   `json:"output_token"`
	InputCost      float64 `json:"input_cost"`
	OutputCost     float64 `json:"output_cost"`
	WaitTime       int64   `json:"wait_time"`
	RequestSuccess int64   `json:"request_success"`
	RequestFailed  int64   `json:"request_failed"`
}

// intDateToString converts int date format (year*1000 + yearday) to YYYYMMDD string
func intDateToString(date int) string {
	year := date / 1000
	yearday := date % 1000
	// Create a time.Time from year and yearday (Jan 1 + yearday - 1)
	t := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, yearday-1)
	return t.Format("20060102")
}

func init() {
	router.NewGroupRouter("/api/v1/stats").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/today", http.MethodGet).
				Handle(getStatsToday),
		).
		AddRoute(
			router.NewRoute("/daily", http.MethodGet).
				Handle(getStatsDaily),
		).
		AddRoute(
			router.NewRoute("/hourly", http.MethodGet).
				Handle(getStatsHourly),
		).
		AddRoute(
			router.NewRoute("/total", http.MethodGet).
				Handle(getStatsTotal),
		).
		AddRoute(
			router.NewRoute("/apikey", http.MethodGet).
				Handle(getStatsAPIKey),
		)
}

func getStatsToday(c *gin.Context) {
	today := op.StatsTodayGet()
	response := StatsDailyResponse{
		Date:           intDateToString(today.Date),
		InputToken:     today.InputToken,
		OutputToken:    today.OutputToken,
		InputCost:      today.InputCost,
		OutputCost:     today.OutputCost,
		WaitTime:       today.WaitTime,
		RequestSuccess: today.RequestSuccess,
		RequestFailed:  today.RequestFailed,
	}
	resp.Success(c, response)
}

func getStatsDaily(c *gin.Context) {
	statsDaily, err := op.StatsGetDaily(c.Request.Context())
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}

	// Convert to response format with string date for frontend compatibility
	response := make([]StatsDailyResponse, len(statsDaily))
	for i, stat := range statsDaily {
		response[i] = StatsDailyResponse{
			Date:           intDateToString(stat.Date),
			InputToken:     stat.InputToken,
			OutputToken:    stat.OutputToken,
			InputCost:      stat.InputCost,
			OutputCost:     stat.OutputCost,
			WaitTime:       stat.WaitTime,
			RequestSuccess: stat.RequestSuccess,
			RequestFailed:  stat.RequestFailed,
		}
	}

	resp.Success(c, response)
}

func getStatsHourly(c *gin.Context) {
	hourly := op.StatsHourlyGet()
	response := make([]StatsHourlyResponse, len(hourly))
	for i, stat := range hourly {
		response[i] = StatsHourlyResponse{
			Hour:           stat.Hour,
			Date:           intDateToString(stat.Date),
			InputToken:     stat.InputToken,
			OutputToken:    stat.OutputToken,
			InputCost:      stat.InputCost,
			OutputCost:     stat.OutputCost,
			WaitTime:       stat.WaitTime,
			RequestSuccess: stat.RequestSuccess,
			RequestFailed:  stat.RequestFailed,
		}
	}
	resp.Success(c, response)
}

func getStatsTotal(c *gin.Context) {
	resp.Success(c, op.StatsTotalGet())
}

func getStatsAPIKey(c *gin.Context) {
	resp.Success(c, op.StatsAPIKeyList())
}
