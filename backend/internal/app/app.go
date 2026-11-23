package app

import (
	"alliance/internal/database"
	"alliance/internal/resources"
	"context"
	"errors"
	"os"
)

func Catalog(ctx context.Context) (resources.Catalog, error) {
	var c resources.Catalog
	vault, err := resources.NewVault(os.Getenv("RESOURCE_KEY"))
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
	return resources.Catalog{DB: db, Vault: vault}, nil
}
