package schema

// Go types. The validator in this package decides what is valid; these are
// models only (--only-models), so no generated code second-guesses it.
//go:generate go tool go-jsonschema --only-models --tags json --capitalization ID,URL,LQIP --struct-name-from-title -p manifest -o ../engine/manifest/types_gen.go manifest.schema.json facts/project.schema.json facts/artwork.schema.json facts/photo.schema.json facts/track.schema.json facts/video.schema.json facts/writing.schema.json facts/collection.schema.json facts/experience.schema.json

// Fixtures derived from testdata/valid/full.json, and their expectations.
//go:generate bash testdata/make-fixtures.sh

// Zod schemas for @canon/client, and the UI's API types from openapi.yaml.
//go:generate sh -c "cd ../client && npm run --silent gen"
//go:generate sh -c "cd ../ui && npx --no-install openapi-typescript ../api/openapi.yaml -o src/gen/api.ts --silent"
