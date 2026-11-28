package startdir

// Check is check, for the tests in package startdir_test. They are an external test package so that this package, as
// the test binary builds it, still imports nothing that imports os, which TestStartsBeforeOS needs: an internal test
// file importing testing would make os start first. This file must import nothing either.
var Check = check
