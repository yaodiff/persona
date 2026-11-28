package assistantwire

import "slices"

// The bus methods the assistant answers through the modules of its kernel (server/internal/assistant/kernel/seam)
// rather than through BusHub.request's own cases. seam.NewBusMethod takes no other name, so a module cannot take a
// method the central dispatch already answers: BusHub.request's cases, the extension manager's or the devices'.
// archguard's TestModuleBusMethodsAreNoCentralCase reads those cases and fails on a listed name among them.
//
// Each domain lists its own methods from an init() in its own file, func init() { addModuleMethods(…) }, as
// inbox_bus.go does, so no central file takes a line for a new one.
var moduleMethods []string

// addModuleMethods lists names as bus methods of a kernel module, from a domain file's init().
func addModuleMethods(names ...string) { moduleMethods = append(moduleMethods, names...) }

// IsModuleMethod says whether name is a bus method a module of the assistant's kernel answers.
func IsModuleMethod(name string) bool { return slices.Contains(moduleMethods, name) }

// ModuleMethods are the bus methods the kernel's modules answer, in the order the domains listed them.
func ModuleMethods() []string { return slices.Clone(moduleMethods) }
