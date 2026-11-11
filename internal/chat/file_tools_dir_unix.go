//go:build unix

package chat

import "golang.org/x/sys/unix"

const fileToolDirectoryOpenFlag = unix.O_DIRECTORY
const fileToolNonblockOpenFlag = unix.O_NONBLOCK
