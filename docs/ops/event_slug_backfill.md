# Слаги событий: backfill (П-5)

Событие без `events.slug` не имеет публичной страницы, а фид теперь отдаёт `slug`.
Новым событиям слаг выдаётся при публикации (POST `.../events/{id}/status` в `published`,
POST `/v1/events/{id}/publications`) и при импорте. Для старых событий:

```
# сухой прогон: печатает, что будет выдано
DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-slug-backfill
# запись
DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-slug-backfill --apply
```

Флаги: `--org <uuid>` одна организация, `--published` только опубликованные.
Правило: `geoslug.Slugify(name)`, при занятости в организации `-2`, `-3`.
Идемпотентно: событие со слагом не выбирается и не перезаписывается.

На проде у образа нет shell, бинарь лежит в образе:
`docker run --rm --entrypoint /app/arena-slug-backfill --env DATABASE_URL=... --env JWT_SIGNING_SECRET=x <image> --apply`.
