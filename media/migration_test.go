package media

import (
	"strings"
	"testing"
)

func TestMigrationSQLCustomIdentifiers(t *testing.T) {
	sql, err := MigrationSQL(Config{Schema:"assets", Table:"file_index"})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(sql, `"assets"."file_index"`) { t.Fatalf("missing qualified table: %s", sql) }
	if !strings.Contains(sql, "pending") || !strings.Contains(sql, "deleting") { t.Fatal("missing recovery states") }
}

func TestMigrationSQLRejectsInjection(t *testing.T) {
	for _, cfg := range []Config{
		{Schema:"public;drop schema public"},
		{Table:"media_objects;drop table users"},
		{Schema:"foo.bar"},
		{Table:"MediaObjects"},
	} {
		if _, err := MigrationSQL(cfg); err == nil { t.Fatalf("accepted invalid identifier: %+v", cfg) }
	}
}

func TestQualifiedTableDefaults(t *testing.T) {
	got, err := qualifiedTable(Config{})
	if err != nil { t.Fatal(err) }
	if got != `"public"."media_objects"` { t.Fatalf("unexpected table: %q", got) }
}
