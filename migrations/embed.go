// Package migrations holds the SQL schema as numbered files, embedded in the
// binary.
//
// They are embedded rather than shipped alongside because the image is built
// FROM scratch: there is no filesystem to read them from and no shell to run
// a migration tool with. The binary applies its own schema or it does not
// start.
package migrations

import "embed"

// FS is every migration file, applied in filename order. Files are named
// `NNNN_name.sql`; the number is the version recorded in schema_migrations.
//
// Migrations are append-only. Editing one that has already been applied
// somewhere is caught at startup by a checksum mismatch rather than silently
// leaving two databases with different schemas.
//
//go:embed *.sql
var FS embed.FS
