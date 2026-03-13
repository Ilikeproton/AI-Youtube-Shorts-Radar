package ranking

import (
	"math"
	"time"
)

type Metrics struct {
	AgeDays       int
	ViewsPerDay   float64
	BreakoutScore float64
}

func Compute(now, publishedAt time.Time, views int64) Metrics {
	ageDays := int(math.Ceil(now.Sub(publishedAt).Hours() / 24))
	if ageDays < 1 {
		ageDays = 1
	}
	viewsPerDay := float64(views) / float64(ageDays)
	score := math.Log(float64(views)+1)*0.35 + math.Log(viewsPerDay+1)*0.65
	return Metrics{
		AgeDays:       ageDays,
		ViewsPerDay:   viewsPerDay,
		BreakoutScore: score,
	}
}
