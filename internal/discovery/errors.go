package discovery

type AuthFailedError struct {
	Message string
}

func (e AuthFailedError) Error() string {
	if e.Message == "" {
		return "auth_failed"
	}
	return e.Message
}
