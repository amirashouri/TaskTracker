package install

import "errors"

func adminListSteps(state State) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
	}
	return []Step{{Kind: StepAdminList, Description: "List admin users", State: state}}, nil
}

func adminResetPasswordSteps(state State, cmd Command) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
	}
	if cmd.AdminEmail == "" || cmd.AdminPassword == "" {
		return nil, errors.New("admin email and password must be provided together")
	}
	return []Step{{
		Kind:        StepAdminResetPassword,
		Description: "Reset admin password",
		State:       state,
		Admin: &AdminBootstrap{
			Email:    cmd.AdminEmail,
			Password: cmd.AdminPassword,
		},
	}}, nil
}
