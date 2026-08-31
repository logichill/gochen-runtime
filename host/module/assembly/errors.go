package assembly

import "gochen/errors"

func wrapModuleErr(moduleID string, err error, message string) errors.IAppError {
	if err == nil {
		return nil
	}
	var appErr *errors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		return appErr.Wrap(message).WithContext("module", moduleID)
	}
	return errors.Wrap(err, errors.Internal, message).WithContext("module", moduleID)
}
