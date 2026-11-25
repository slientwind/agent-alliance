package app

import (
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/llmgateway"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"context"
	"errors"
	"os"
	"strings"
)

func Catalog(ctx context.Context) (resources.Catalog, error) {
	var c resources.Catalog
	var priorKeys []string
	if value := strings.TrimSpace(os.Getenv("RESOURCE_PREVIOUS_KEYS")); value != "" {
		priorKeys = strings.Split(value, ",")
	}
	vault, err := resources.NewVault(os.Getenv("RESOURCE_KEY"), priorKeys...)
	if err != nil {
		return c, err
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	db, err := database.Open(ctx, url)
	if err != nil {
		return c, err
	}
	if err := egress.ImportLegacy(ctx, db, os.Getenv("AGENT_ALLOWED_HOSTS")); err != nil {
		db.Close()
		return c, err
	}
	return resources.Catalog{DB: db, Vault: vault}, nil
}

func ModelGateway(c resources.Catalog) llmgateway.Gateway {
	return llmgateway.Gateway{
		Policy:     &policy.Policy{DB: c.DB},
		Catalog:    llmgateway.Catalog{DB: c.DB},
		Provider:   llmgateway.HTTPProvider{Vault: c.Vault, Egress: &egress.Policy{DB: c.DB}},
		Budget:     llmgateway.Budget{DB: c.DB},
		Resilience: llmgateway.Resilience{DB: c.DB},
	}
}
