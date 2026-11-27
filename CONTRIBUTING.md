# Contributing to Agent Alliance

Thank you for your interest in contributing to **Agent Alliance**! Whether you are fixing a bug, adding an ERP/CRM/E-commerce connector, improving documentation, or building an enterprise Skill pack, contributions are welcome.

## Development Environment

- **Go**: 1.26+
- **Node.js**: 22+
- **Python**: 3.12+ (for `connectors/`, `preview/`, `retrieval/`)
- **Docker & Docker Compose** (for PostgreSQL + pgvector, Temporal, and air-gapped sandbox)

```bash
make infra
# In separate terminals:
make api
make worker
make tools
make web
```

## High-Impact Areas for Contributions

1. **Business System Connectors (`backend/internal/bizconnectors/`)**: Add read/L2-writeback adapters for additional ERP, CRM, e-commerce, and ITSM suites (e.g., Shopify, Youzan, SAP OData, Jira, HubSpot).
2. **Enterprise IM Channels (`backend/internal/channels/`)**: Extend interactive approval cards and event delivery for Slack, WeCom, Microsoft Teams, or DingTalk.
3. **Knowledge Sync Sources (`backend/internal/knowledgesync/`)**: Add incremental directory sync connectors (Notion, Google Drive, GitLab Wiki).
4. **Documentation & Internationalization**: Improve English deployment guides, connector tutorials, and example corporate templates.

## Pull Request Guidelines

1. Fork the repository and create a feature branch from `main`.
2. Keep changes focused and preserve security invariants (no raw credential logging, strict OBO/RLS checks, AST read-only SQL guards, L2 confirmation for external mutations).
3. Run unit tests before opening a PR:
   ```bash
   cd backend && go test ./...
   cd frontend && npm run build && npm test
   ```
4. Follow our [Code of Conduct](CODE_OF_CONDUCT.md) and reference any related Issue in your PR description.
