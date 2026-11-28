module quicknotes

go 1.24

require github.com/nwverse/persona/sdk/go v0.0.0

// In this repository the sample builds against the SDK beside it. An extension of your own carries a copy of the SDK
// instead (personad extension new writes ./sdk/go and points this line at it).
replace github.com/nwverse/persona/sdk/go => ../../go
