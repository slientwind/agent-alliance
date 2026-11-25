package runtime

import (
	"fmt"

	"alliance/internal/policy"
	"alliance/internal/resources"
)

func (a Activities) setExternalDLP(model *resources.Model, step Step, attempt int, actor string) {
	model.ExternalContent = policy.ExternalDLP{
		DB: a.Catalog.DB, Vault: a.Catalog.Vault, Run: step.Run, Actor: actor,
		Exchange: fmt.Sprintf("plan/%d/%d", step.Ordinal, attempt), Endpoint: model.Endpoint,
	}
}
