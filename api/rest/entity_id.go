package rest

import (
	"gochen/domain"
	"gochen/errors"
)

// ensureEntityIDMatchesPath 确保实体 ID 与路径 ID 匹配。
func ensureEntityIDMatchesPath[T domain.IEntity[ID], ID comparable](entity *T, pathID ID) error {
	if entity == nil {
		return errors.NewCode(errors.InvalidInput, "entity cannot be nil")
	}
	current := (*entity).GetID()
	if current == pathID {
		return nil
	}

	var zero ID
	// 若 body 未携带 ID（零值），尽力将 ID 回填为 path ID（仅当实体支持 SetID）。
	if current == zero {
		if settable, ok := any(*entity).(domain.ISettableID[ID]); ok {
			settable.SetID(pathID)
			if (*entity).GetID() == pathID {
				return nil
			}
		}
		if settable, ok := any(entity).(domain.ISettableID[ID]); ok {
			settable.SetID(pathID)
			if (*entity).GetID() == pathID {
				return nil
			}
		}
		return errors.NewCode(errors.InvalidInput, "entity ID must match path id").WithContext("path_id", pathID)
	}

	// body 显式携带了不同的 ID：拒绝，避免误更新/潜在越权。
	return errors.NewCode(errors.Validation, "path id and body id mismatch").
		WithContext("path_id", pathID).
		WithContext("body_id", current)
}
