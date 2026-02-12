package analyzer

import (
	"time"

	"github.com/nickel-chromium/tabsordnung/internal/types"
)

func AnalyzeStale(tabs []*types.Tab, thresholdDays int) {
	threshold := time.Duration(thresholdDays) * 24 * time.Hour
	now := time.Now()

	for _, tab := range tabs {
		age := now.Sub(tab.LastAccessed)
		if age > threshold {
			tab.IsStale = true
			tab.StaleDays = int(age.Hours() / 24)
		}
	}
}
