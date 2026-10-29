package dailybilling

import (
	"context"
	"sync"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Reporter serves the management daily-billing endpoint.
type Reporter interface {
	Report(ctx context.Context, query Query) (Report, error)
	DefaultTimezone() string
}

// Service loads new-api and CPA aggregates and allocates them.
type Service struct {
	cfg   Config
	cpa   *gorm.DB
	mu    sync.Mutex
	api   *gorm.DB
	logs  logSource
	tiers tierSource
}

type logSource interface {
	Aggregate(ctx context.Context, cfg Config, query Query) ([]LogRow, error)
}

type tierSource interface {
	Aggregate(ctx context.Context, cfg Config, query Query) ([]TierRow, error)
}

type sqlLogSource struct{ svc *Service }

func (s sqlLogSource) Aggregate(ctx context.Context, cfg Config, query Query) ([]LogRow, error) {
	db, errDB := s.svc.readOnlyDB(ctx)
	if errDB != nil {
		return nil, errDB
	}
	return queryNewAPILogs(ctx, db, cfg, query)
}

type sqlTierSource struct{ db *gorm.DB }

func (s sqlTierSource) Aggregate(ctx context.Context, cfg Config, query Query) ([]TierRow, error) {
	return queryCPATiers(ctx, s.db, cfg, query)
}

// NewService builds a reporter. An unconfigured cfg never opens a database.
func NewService(cfg Config, cpa *gorm.DB) *Service {
	cfg = cfg.normalized()
	svc := &Service{cfg: cfg, cpa: cpa}
	svc.logs = sqlLogSource{svc: svc}
	svc.tiers = sqlTierSource{db: cpa}
	return svc
}

// DefaultTimezone is the reporting zone used when the request omits one.
func (s *Service) DefaultTimezone() string {
	if s == nil || s.cfg.Timezone == "" {
		return DefaultTimezone
	}
	return s.cfg.Timezone
}

// Report returns the estimate, or a configured=false payload when new-api is unset.
func (s *Service) Report(ctx context.Context, query Query) (Report, error) {
	if s == nil || !s.cfg.Configured() {
		return Report{Configured: false, Message: UnconfiguredMessage, Days: []Day{}}, nil
	}
	logs, errLogs := s.logs.Aggregate(ctx, s.cfg, query)
	if errLogs != nil {
		return Report{}, errLogs
	}
	tiers, errTiers := s.tiers.Aggregate(ctx, s.cfg, query)
	if errTiers != nil {
		return Report{}, errTiers
	}
	return Build(s.cfg, query, logs, tiers), nil
}

func (s *Service) readOnlyDB(ctx context.Context) (*gorm.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.api != nil {
		return s.api, nil
	}
	db, errOpen := openReadOnly(ctx, s.cfg)
	if errOpen != nil {
		dsn, _ := s.cfg.connectionString()
		log.Warnf("new-api billing source unavailable: %s", redactSecrets(errOpen.Error(), s.cfg.Password, dsn))
		return nil, ErrSourceUnavailable
	}
	s.api = db
	log.Infof("new-api billing source connected %s", s.cfg.SafeString())
	return db, nil
}
