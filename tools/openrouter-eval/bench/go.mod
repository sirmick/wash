// Benchmarks are their own module so the repository's go test ./... skips
// them: their hidden tests only compile inside a finished project.
module bench

go 1.22
