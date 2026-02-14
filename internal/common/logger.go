package common

import (
	"github.com/rs/zerolog/log"
	container "github.com/thehyperflames/dicontainer-go"
)

// ServiceLogger provides structured logging for DI services
type ServiceLogger struct {
	svc container.IInstance

	debug        bool
	whiteListSvc map[string]map[string]struct{}
}

// NewServiceLogger creates a new logger for a service
func NewServiceLogger(svc container.IInstance) *ServiceLogger {
	return &ServiceLogger{svc: svc, debug: false, whiteListSvc: make(map[string]map[string]struct{})}
}

func (l *ServiceLogger) SetDebugMode(debug bool) {
	l.debug = debug
}

func (l *ServiceLogger) EnableLogForServices(svc []string) {
	for _, s := range svc {
		l.whiteListSvc[s] = make(map[string]struct{})
	}
}

func (l *ServiceLogger) EnableMethodLogForService(svc string, method string) {
	if _, ok := l.whiteListSvc[svc]; ok {
		if _, ok := l.whiteListSvc[svc][method]; ok {
			l.whiteListSvc[svc][method] = struct{}{}
		}
	}
}

func (l *ServiceLogger) Info(msg string, method string) string {
	if l.debug {
		if _, ok := l.whiteListSvc[l.svc.ID()]; ok {
			methods := l.whiteListSvc[l.svc.ID()]
			if len(methods) == 0 {
				log.Info().Str("service", l.svc.ID()).Str("method", method).Msg(msg)
			} else {
				if _, ok := l.whiteListSvc[l.svc.ID()][method]; ok {
					log.Info().Str("service", l.svc.ID()).Str("method", method).Msg(msg)
				}
			}
		}
	}
	return msg
}

func (l *ServiceLogger) Error(err error, msg string, method string) string {
	if l.debug {
		if _, ok := l.whiteListSvc[l.svc.ID()]; ok {
			methods := l.whiteListSvc[l.svc.ID()]
			if len(methods) == 0 {
				log.Error().Str("service", l.svc.ID()).Str("method", method).Err(err).Msg(msg)
			} else {
				if _, ok := l.whiteListSvc[l.svc.ID()][method]; ok {
					log.Error().Str("service", l.svc.ID()).Str("method", method).Err(err).Msg(msg)
				}
			}
		}
	}
	return msg
}
