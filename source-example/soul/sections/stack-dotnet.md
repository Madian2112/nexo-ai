---
inclusion: fileMatch
description: C# and .NET rules.
globs: ["**/*.cs", "**/*.csproj", "**/*.sln"]
---
# .NET rules

- Async all the way: no `.Result` or `.Wait()`.
- Controllers stay thin; logic lives in application services.
- Use NVARCHAR for text columns.
