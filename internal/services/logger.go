package services

import (
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type ServiceIdentifier interface {
	ID() string
}

type ServiceLogger struct {
	logger zerolog.Logger
}

func NewServiceLogger(svc ServiceIdentifier) *ServiceLogger {
	return &ServiceLogger{
		logger: log.With().Str("service", svc.ID()).Logger(),
	}
}

func (l *ServiceLogger) Info() *zerolog.Event {
	return l.logger.Info()
}

func (l *ServiceLogger) Error() *zerolog.Event {
	return l.logger.Error()
}

func (l *ServiceLogger) Warn() *zerolog.Event {
	return l.logger.Warn()
}

func (l *ServiceLogger) Debug() *zerolog.Event {
	return l.logger.Debug()
}
