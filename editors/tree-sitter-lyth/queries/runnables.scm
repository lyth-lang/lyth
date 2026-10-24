; `func main` runs the program
((function_declaration
  !receiver
  name: (identifier) @_name) @run
  (#eq? @_name "main")
  (#set! tag lyth-main))

; `func TestXxx(t: *testing.T)` (and benchmarks, fuzz tests, examples) in a _test.lyth file runs one test
((function_declaration
  !receiver
  name: (identifier) @LYTH_TEST_NAME) @run
  (#match? @LYTH_TEST_NAME "^(Test|Benchmark|Fuzz|Example)")
  (#set! tag lyth-test))
