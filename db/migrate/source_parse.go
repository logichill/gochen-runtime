package migrate

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gochen-runtime/db/sql/safeident"
	"gochen/errors"
)

var (
	fileNamePattern      = regexp.MustCompile(`^([0-9]+)_([^.]+)\.(up|down)\.sql$`)
	typedFileNamePattern = regexp.MustCompile(`^([0-9]+)\.([A-Za-z_][A-Za-z0-9_]*)\.([^.]+)\.(up|down)\.sql$`)
)

func parseMigrationFileName(name string) (File, bool, error) {
	if matches := typedFileNamePattern.FindStringSubmatch(name); len(matches) > 0 {
		return parseMigrationFileParts(name, matches[1], matches[2], matches[3], matches[4])
	}
	matches := fileNamePattern.FindStringSubmatch(name)
	if len(matches) > 0 {
		return parseMigrationFileParts(name, matches[1], defaultMigrationType, matches[2], matches[3])
	}
	return File{}, false, nil
}

func parseMigrationFileParts(fileName string, versionText string, migrationType string, migrationName string, directionText string) (File, bool, error) {
	version, err := strconv.ParseUint(versionText, 10, 64)
	if err != nil {
		return File{}, false, errors.Wrap(err, errors.InvalidInput, "parse migration version failed").
			WithContext("file", fileName)
	}
	if version == 0 {
		return File{}, false, errors.NewCode(errors.InvalidInput, "migration version must be greater than zero").
			WithContext("file", fileName)
	}
	if !isSafeMigrationType(migrationType) {
		return File{}, false, errors.NewCode(errors.InvalidInput, "invalid migration type").
			WithContext("file", fileName).
			WithContext("type", migrationType)
	}
	if isReservedMigrationType(migrationType) {
		return File{}, false, errors.NewCode(errors.InvalidInput, "migration type is reserved").
			WithContext("file", fileName).
			WithContext("type", migrationType)
	}

	direction := Direction(directionText)
	if direction != DirectionUp && direction != DirectionDown {
		return File{}, false, errors.NewCode(errors.InvalidInput, "unsupported migration direction").
			WithContext("file", fileName)
	}

	return File{
		Type:      migrationType,
		Version:   version,
		Name:      migrationName,
		Direction: direction,
	}, true, nil
}

func isSafeMigrationType(migrationType string) bool {
	return safeident.IsSafeIdentifier(migrationType) && !strings.Contains(migrationType, ".")
}

func isReservedMigrationType(migrationType string) bool {
	return strings.EqualFold(strings.TrimSpace(migrationType), lockMigrationType)
}

func groupMigrationFiles(files []File) ([]Migration, error) {
	type key struct {
		migrationType string
		version       uint64
	}
	byVersion := make(map[key]*Migration)
	for _, file := range files {
		migrationType := file.Type
		if migrationType == "" {
			migrationType = defaultMigrationType
		}
		k := key{migrationType: migrationType, version: file.Version}
		migration := byVersion[k]
		if migration == nil {
			migration = &Migration{Type: migrationType, Version: file.Version, Name: file.Name}
			byVersion[k] = migration
		}
		if migration.Name != file.Name {
			return nil, errors.NewCode(errors.Conflict, "migration version maps to multiple names").
				WithContext("type", migrationType).
				WithContext("version", file.Version).
				WithContext("name", migration.Name).
				WithContext("conflict_name", file.Name)
		}
		switch file.Direction {
		case DirectionUp:
			if migration.Up != nil {
				return nil, errors.NewCode(errors.Conflict, "duplicated up migration").
					WithContext("type", migrationType).
					WithContext("version", file.Version).
					WithContext("path", file.fullName())
			}
			cp := file
			migration.Up = &cp
		case DirectionDown:
			if migration.Down != nil {
				return nil, errors.NewCode(errors.Conflict, "duplicated down migration").
					WithContext("type", migrationType).
					WithContext("version", file.Version).
					WithContext("path", file.fullName())
			}
			cp := file
			migration.Down = &cp
		}
	}

	keys := make([]key, 0, len(byVersion))
	for k := range byVersion {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].migrationType == keys[j].migrationType {
			return keys[i].version < keys[j].version
		}
		return keys[i].migrationType < keys[j].migrationType
	})

	migrations := make([]Migration, 0, len(keys))
	for _, k := range keys {
		migrations = append(migrations, *byVersion[k])
	}
	return migrations, nil
}

func (m Migration) String() string {
	if m.Name == "" {
		return fmt.Sprintf("%d", m.Version)
	}
	return fmt.Sprintf("%d_%s", m.Version, m.Name)
}
