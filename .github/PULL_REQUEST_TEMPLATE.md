# Pull Request

## Summary

<!-- Briefly describe what this PR changes and why. -->

## Type of change

- [ ] Bug fix
- [ ] New feature
- [ ] Refactor / chore
- [ ] Documentation only
- [ ] Build / CI

## Checklist

- [ ] `make lint` passes (or lint baseline unchanged).
- [ ] `make test` passes locally.
- [ ] OpenAPI changes regenerated (`make gen-openapi` / `make gen-ts-client`).
- [ ] Migrations are forward-only and idempotent (if applicable).
- [ ] Audit-log events covered for new write actions (if applicable).
- [ ] **If this PR changes any `platform_superadmin` functionality
      (admin-panel menu items, roles/permissions, webhook events, payment
      configs, audit log, reason-context flow, etc.), `docs/ru/superadmin_guide.md`
      is updated in the same PR.**

## Related

<!-- Link issues / features / specs this PR addresses. -->
