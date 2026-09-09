package envbridge

import "os"

const platformSupported = false

// Windows ACLs are not represented by Unix mode bits. Refuse the bridge until
// an equivalent ownership/access check exists; other applets remain available.
func ownedByCurrentUser(os.FileInfo) bool { return false }
