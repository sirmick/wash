# Minimal build for the cpp-about example. CMake embeds assets/index.js
# and links cpp-sdk's wire client; no wlroots or other native deps.

out/cpp-about: main.cpp assets/index.js CMakeLists.txt
	cmake -S . -B build -DCMAKE_BUILD_TYPE=Release >/dev/null
	cmake --build build
	@mkdir -p out && cp build/cpp-about out/cpp-about

.PHONY: manifest
manifest: out/cpp-about
	./out/cpp-about --wash-manifest | head -c 400; echo

.PHONY: clean
clean:
	rm -rf out build
