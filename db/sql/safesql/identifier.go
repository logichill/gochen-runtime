package safesql

import (
	"gochen-runtime/db/sql/safeident"
	"gochen/db/dialect"
)

// QuoteQualifiedIdentifier 校验限定标识符并按方言 quote。
func QuoteQualifiedIdentifier(d dialect.IDialect, name, fallback, source string, maxParts int) (string, error) {
	normalized, err := safeident.NormalizeQualifiedIdentifier(name, fallback, source, maxParts)
	if err != nil {
		return "", err
	}
	return d.QuoteIdentifier(normalized), nil
}
