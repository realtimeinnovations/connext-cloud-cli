# Output policy

CLI output uses `fileoutput.WriteFile` (bytes), `Copy` (streams), or
`OpenAppend` (logs). Use `Validate` for preflight and `ValidateWithin` when a
generated path must stay inside an output directory. `Inspect` distinguishes
missing destinations from inspection failures; only missing paths are allowed
by preflight. `MkdirAll` applies the same policy to directory creation.

Existing ancestors must be directories. File destinations must be regular
files. Symlinks, dangling links, and special files are rejected, including
ancestors above the chosen output directory. Paths are normalized before use;
macOS's fixed `/var`, `/tmp`, and `/etc` aliases are accepted only when they point
to their standard `/private` locations. Arbitrary links are never resolved.

Replacement writes stage complete contents in the destination directory and
set permissions before publishing. The requested mode cannot loosen existing
file permissions. Callers select the mode: credentials and private keys use
`0600`, ordinary output generally uses `0644`. New parents for private writes
use `0700`; existing directory permissions are retained. Replacement requires
a writable parent and replaces the inode, so other hard links retain their old
contents. Append streams keep the existing inode.

Permission-bit guarantees above apply to POSIX systems. On Windows, Go's mode
bits only control the read-only attribute: `0600` does not establish an
owner-only ACL. Staged files inherit the parent directory's ACL, and replacement
does not preserve a destination's custom ACL. Native Windows CI exercises path
validation and write behavior; passing it does not establish ACL protection.
Windows replacement can also fail when an existing reader denies delete sharing.

Callers retain their overwrite, force, and editable-example preservation rules.
Validate an entire batch before writing when a known bad destination must block
all output, and preflight credential destinations before registration.
`common.DecodeSecureFiles` validates all server filenames and decodes the whole
response before saving it.

Specialized operations retain their publication contracts: installer locking
keeps its inode, migration uses no-clobber publication, downloads verify their
digest, license repair replaces invalid entries, and the updater uses exclusive
staging plus rollback. They use shared path validation for their destinations or,
for explicit license repair, their parent directories.

These checks are path-based, not a sandbox against concurrent ancestor swaps.
Preflight does not guarantee a later write will succeed. Multiple file writes
are not a transaction, and publication does not imply crash durability.

Filesystem edge cases belong in the utility's test matrix. Caller tests check
production wiring, permission selection, overwrite behavior, and whether
preflight precedes side effects; they should assert observable contents and
effects rather than helper call sequences.
