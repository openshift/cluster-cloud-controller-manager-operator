
#!/bin/bash

function list_go_src_files() {
	find . -not \( \
		\( \
		-wholename './.*' \
		-o -wholename '*/vendor/*' \
		\) -prune \
	\) -name '*.go' | sort -u
}


bad_files=$(list_go_src_files | xargs gofmt -s -l)
if [[ -n "${bad_files}" ]]; then
        echo "Here is a list of badly formatted files:"
        echo "${bad_files}"
        echo "Try running 'make update-fmt' to fix them."
        exit 1
fi
