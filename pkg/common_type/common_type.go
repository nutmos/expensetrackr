package common_type

import "k8s.io/utils/ptr"

type UID string

func (uid UID) String() string {
	return string(uid)
}

func UIDPtr(uid string) *UID {
	return ptr.To(UID(uid))
}
