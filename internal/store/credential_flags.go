package store

type CredentialFlags uint64

const (
	CredentialEnabled CredentialFlags = 1 << iota
	CredentialList
	CredentialRead
	CredentialWrite
)

func (f CredentialFlags) Has(flag CredentialFlags) bool                { return f&flag == flag }
func (f CredentialFlags) With(flag CredentialFlags) CredentialFlags    { return f | flag }
func (f CredentialFlags) Without(flag CredentialFlags) CredentialFlags { return f &^ flag }

func (f CredentialFlags) Names() []string {
	var names []string
	for _, item := range []struct {
		name string
		flag CredentialFlags
	}{
		{"list", CredentialList}, {"read", CredentialRead}, {"write", CredentialWrite},
	} {
		if f.Has(item.flag) {
			names = append(names, item.name)
		}
	}
	return names
}
