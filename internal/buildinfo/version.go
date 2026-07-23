package buildinfo

// Version is replaced at link time by the Makefile. Builds that do not inject
// repository or release metadata report "unknown".
var Version = "unknown"
