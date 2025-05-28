package appconfig

import "time"

type Config struct {
	ErrorReconcileTime   time.Duration
	SuccessReconcileTime time.Duration
	UpgradeFrequency     time.Duration
}

func NewConfig(errorReconcileTime, successReconcileTime, upgradeFrequency time.Duration) *Config {
	return &Config{
		ErrorReconcileTime:   errorReconcileTime,
		SuccessReconcileTime: successReconcileTime,
		UpgradeFrequency:     upgradeFrequency,
	}
}
