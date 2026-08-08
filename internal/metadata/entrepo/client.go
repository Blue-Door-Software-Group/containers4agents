package entrepo

import (
	"database/sql"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/Blue-Door-Software-Group/containers4agents/ent"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
)

type Client struct {
	Ent *ent.Client
	DB  *sql.DB
}

func New(cfg config.Config) (*Client, error) {
	drv, err := entsql.Open(cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open sql driver: %w", err)
	}

	db := drv.DB()
	db.SetMaxIdleConns(10)
	db.SetMaxOpenConns(50)
	db.SetConnMaxLifetime(time.Hour)

	entClient := ent.NewClient(ent.Driver(drv))
	return &Client{Ent: entClient, DB: db}, nil
}

func (c *Client) Close() error {
	if c == nil || c.Ent == nil {
		return nil
	}
	return c.Ent.Close()
}
