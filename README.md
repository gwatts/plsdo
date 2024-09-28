# plsdo

Experimental cli wrapper around gopls.

## Method invocations

Pretty print all invocations of methods or functions within the current module

e.g to find all places that  Zap Logger Info or Warn methods are used:


```go
$ plsdo refs go.uber.org/zap Logger.Info Logger.Warn
```


Output as JSON:

```go
$ plsdo refs --format json go.uber.org/zap Logger.Info
```

Use Globs

```go
$ plsdo refs --format json go.uber.org/zap '*Logger.*'
```
