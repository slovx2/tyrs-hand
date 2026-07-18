package logging

import (
	"github.com/slovx2/tyrs-hand/internal/config"
	"go.uber.org/zap"
)

func New(cfg config.Config) (*zap.Logger, error) {
	if cfg.Environment == "development" {
		return zap.NewDevelopment()
	}
	return zap.NewProduction()
}
