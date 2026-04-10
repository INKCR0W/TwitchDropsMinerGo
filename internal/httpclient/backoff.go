package httpclient

import (
	"fmt"
	"math"
	"math/rand"
	"time"
)

type BackoffConfig struct {
	Base     float64
	Variance float64
	Shift    time.Duration
	Maximum  time.Duration
}

func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		Base:     2,
		Variance: 0.1,
		Maximum:  3 * time.Minute,
	}
}

type ExponentialBackoff struct {
	config  BackoffConfig
	steps   int
	random  func() float64
	maximum time.Duration
}

func NewExponentialBackoff(config BackoffConfig) (*ExponentialBackoff, error) {
	if config.Base <= 1 {
		return nil, fmt.Errorf("退避基数必须大于 1")
	}
	if config.Variance < 0 {
		return nil, fmt.Errorf("退避抖动不能小于 0")
	}
	if config.Maximum <= 0 {
		return nil, fmt.Errorf("最大退避时间必须大于 0")
	}

	return &ExponentialBackoff{
		config:  config,
		random:  rand.Float64,
		maximum: config.Maximum,
	}, nil
}

func (b *ExponentialBackoff) Next() time.Duration {
	if b == nil {
		return 0
	}

	multiplier := 1.0
	if b.config.Variance > 0 {
		minimum := 1 - b.config.Variance
		maximum := 1 + b.config.Variance
		multiplier = minimum + (maximum-minimum)*b.random()
	}

	seconds := math.Pow(b.config.Base, float64(b.steps))*multiplier + b.config.Shift.Seconds()
	delay := time.Duration(seconds * float64(time.Second))
	if delay > b.maximum {
		return b.maximum
	}

	b.steps++
	return delay
}

func (b *ExponentialBackoff) Reset() {
	if b == nil {
		return
	}

	b.steps = 0
}
