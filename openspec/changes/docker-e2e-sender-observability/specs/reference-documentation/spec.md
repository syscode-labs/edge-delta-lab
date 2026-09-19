## ADDED Requirements

### Requirement: Provide editable architecture diagrams
The repository SHALL include native Excalidraw source files for architecture,
chunk reuse, and interrupted delivery, plus previews readable from the README.

#### Scenario: An engineer changes the design
- **WHEN** the engineer opens the .excalidraw file in Excalidraw
- **THEN** diagram text, shapes and connectors remain editable rather than a flattened image

### Requirement: Give a plain-English primary walkthrough
The README SHALL explain the goal, prerequisites, main Docker command, sender CLI,
measurement meanings, file locations and troubleshooting without requiring prior chat context.

#### Scenario: A new engineer extracts the repository
- **WHEN** the engineer follows the README
- **THEN** they can run the main demo and find measured results and live sender status
- **AND** they can distinguish executed tests from pending Docker or hardware validation

### Requirement: Preserve evidence boundaries
Documentation SHALL distinguish synthetic transport measurements, genuine Docker
execution, and unexecuted production validation tasks.

#### Scenario: Only the transport demo has run in the authoring environment
- **WHEN** results are packaged for handoff
- **THEN** Docker execution is explicitly NOT RUN rather than marked successful
