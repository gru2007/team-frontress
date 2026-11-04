# Team Frontress UI previewer. See main.cpp.
#   cmake -S tools/fui-preview -B build/fui-preview -G Ninja && ninja -C build/fui-preview
cmake_minimum_required(VERSION 3.16)
project(fui_preview CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
	set(CMAKE_BUILD_TYPE Release)
endif()

get_filename_component(REPO "${CMAKE_CURRENT_SOURCE_DIR}/../.." ABSOLUTE)
set(RMLUI "${REPO}/src/thirdparty/RmlUi")

file(GLOB RMLUI_SOURCES "${RMLUI}/Source/Core/*.cpp" "${RMLUI}/Source/Core/Elements/*.cpp" "${RMLUI}/Source/Core/Layout/*.cpp" "${RMLUI}/Source/Debugger/*.cpp")
file(GLOB FUI_SOURCES "${REPO}/src/frontress_ui/*.cpp")

find_package(SDL3 REQUIRED CONFIG)
find_package(OpenGL REQUIRED)

add_executable(fui-preview main.cpp tf2content.cpp ${FUI_SOURCES} ${RMLUI_SOURCES})
target_include_directories(fui-preview PRIVATE "${RMLUI}/Include" "${REPO}/src/frontress_ui")
target_compile_definitions(fui-preview PRIVATE RMLUI_STATIC_LIB FUI_WITH_DEBUGGER FUI_PREVIEW_REPO="${REPO}")
target_link_libraries(fui-preview PRIVATE SDL3::SDL3 OpenGL::GL)
