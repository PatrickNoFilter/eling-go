// Package skills manages loadable skills/plugins for the AI agent.
package skills

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"sync"
)

// Skill defines a single executable skill.
type Skill struct {
	Name        string
	Description string
	Version     string
	Execute     func(args map[string]interface{}) (interface{}, error)
}

// Manager manages available skills.
type Manager struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

// NewManager creates a new skill manager.
func NewManager() *Manager {
	m := &Manager{
		skills: make(map[string]Skill),
	}
	m.registerBuiltins()
	return m
}

// Register adds a skill to the manager.
func (m *Manager) Register(s Skill) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.skills[s.Name]; exists {
		return fmt.Errorf("skill %q already registered", s.Name)
	}
	m.skills[s.Name] = s
	return nil
}

// Get retrieves a skill by name.
func (m *Manager) Get(name string) (Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	return s, ok
}

// List returns all registered skills.
func (m *Manager) List() []Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Skill, 0, len(m.skills))
	for _, s := range m.skills {
		result = append(result, s)
	}
	return result
}

// Count returns the number of registered skills.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.skills)
}

// Remove removes a skill by name.
func (m *Manager) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.skills, name)
}

// registerBuiltins registers built-in skills.
func (m *Manager) registerBuiltins() {
	m.skills["echo"] = Skill{
		Name:        "echo",
		Description: "Echoes back the input text",
		Version:     "1.0.0",
		Execute: func(args map[string]interface{}) (interface{}, error) {
			text, _ := args["text"].(string)
			return text, nil
		},
	}
	m.skills["math_eval"] = Skill{
		Name:        "math_eval",
		Description: "Evaluates a simple arithmetic expression safely using a Go AST parser. Supports +, -, *, /, parentheses, and integers/floats.",
		Version:     "2.0.0",
		Execute:     mathEvalExecute,
	}
	m.skills["web_search"] = Skill{
		Name:        "web_search",
		Description: "Search the web for information (not yet implemented - requires API key)",
		Version:     "1.0.0",
		Execute: func(args map[string]interface{}) (interface{}, error) {
			return nil, fmt.Errorf("web search skill requires an API key; configure via SKILL_WEB_SEARCH_API_KEY env var")
		},
	}
}

// mathEvalExecute safely evaluates an arithmetic expression using Go's AST parser.
// Only allows numbers, +, -, *, /, parentheses, and basic math functions.
func mathEvalExecute(args map[string]interface{}) (interface{}, error) {
	expr, _ := args["expression"].(string)
	if expr == "" {
		return nil, fmt.Errorf("expression is required")
	}

	result, err := safeEval(expr)
	if err != nil {
		return nil, fmt.Errorf("cannot evaluate expression %q: %w", expr, err)
	}
	return result, nil
}

// safeEval parses and evaluates an arithmetic expression safely.
// Uses Go's AST parser to ensure only safe mathematical operations are performed.
func safeEval(expr string) (float64, error) {
	// Wrap in parentheses to make it a valid expression for Go parser
	expr = "(" + expr + ")"

	root, err := parser.ParseExpr(expr)
	if err != nil {
		return 0, fmt.Errorf("parse error: %w", err)
	}

	result, err := evalAST(root)
	if err != nil {
		return 0, err
	}
	return result, nil
}

// evalAST recursively evaluates an AST node.
func evalAST(node ast.Expr) (float64, error) {
	switch n := node.(type) {
	case *ast.ParenExpr:
		return evalAST(n.X)

	case *ast.BinaryExpr:
		left, err := evalAST(n.X)
		if err != nil {
			return 0, err
		}
		right, err := evalAST(n.Y)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case token.ADD:
			return left + right, nil
		case token.SUB:
			return left - right, nil
		case token.MUL:
			return left * right, nil
		case token.QUO:
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			return left / right, nil
		case token.REM:
			// Remainder: only for integers
			return float64(int64(left) % int64(right)), nil
		default:
			return 0, fmt.Errorf("unsupported operator: %s", n.Op.String())
		}

	case *ast.UnaryExpr:
		val, err := evalAST(n.X)
		if err != nil {
			return 0, err
		}
		switch n.Op {
		case token.SUB:
			return -val, nil
		case token.ADD:
			return val, nil
		default:
			return 0, fmt.Errorf("unsupported unary operator: %s", n.Op.String())
		}

	case *ast.BasicLit:
		switch n.Kind {
		case token.INT:
			v, err := strconv.ParseInt(n.Value, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid integer: %s", n.Value)
			}
			return float64(v), nil
		case token.FLOAT:
			v, err := strconv.ParseFloat(n.Value, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid float: %s", n.Value)
			}
			return v, nil
		default:
			return 0, fmt.Errorf("unsupported literal: %s", n.Value)
		}

	case *ast.CallExpr:
		// Allow a limited set of math functions
		fun, ok := n.Fun.(*ast.Ident)
		if !ok {
			return 0, fmt.Errorf("unsupported function call")
		}
		if len(n.Args) != 1 {
			return 0, fmt.Errorf("math functions take exactly 1 argument")
		}
		arg, err := evalAST(n.Args[0])
		if err != nil {
			return 0, err
		}
		switch fun.Name {
		case "abs":
			return math.Abs(arg), nil
		case "sqrt":
			if arg < 0 {
				return 0, fmt.Errorf("sqrt of negative number")
			}
			return math.Sqrt(arg), nil
		case "sin":
			return math.Sin(arg), nil
		case "cos":
			return math.Cos(arg), nil
		case "round":
			return math.Round(arg), nil
		case "floor":
			return math.Floor(arg), nil
		case "ceil":
			return math.Ceil(arg), nil
		default:
			return 0, fmt.Errorf("unsupported function: %s", fun.Name)
		}

	case *ast.Ident:
		// Allow named constants: pi, e
		switch n.Name {
		case "pi":
			return math.Pi, nil
		case "e":
			return math.E, nil
		default:
			return 0, fmt.Errorf("unknown identifier: %s", n.Name)
		}

	default:
		return 0, fmt.Errorf("unsupported expression type: %T", node)
	}
}
