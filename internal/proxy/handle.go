package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
)

// handle answers a call of the commands in the shell (rpc.Serve).
func (p *Proxy) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	h, ok := handlers[method]
	if !ok {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	return h(p, ctx, params)
}

// handler answers one RPC method. Who may call it is its own to check:
// fromShell, p.asking, p.handed.
type handler func(p *Proxy, ctx context.Context, params json.RawMessage) (any, error)

// decoded is a handler whose params decode into a T.
func decoded[T any](f func(p *Proxy, ctx context.Context, v T) (any, error)) handler {
	return func(p *Proxy, ctx context.Context, params json.RawMessage) (any, error) {
		var v T
		if err := json.Unmarshal(params, &v); err != nil {
			return nil, err
		}
		return f(p, ctx, v)
	}
}

var handlers = map[string]handler{
	rpc.MethodInfo: func(p *Proxy, _ context.Context, _ json.RawMessage) (any, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.info(), nil
	},
	rpc.MethodModel: decoded(func(p *Proxy, ctx context.Context, mp rpc.ModelParams) (any, error) {
		if mp.Model == "" {
			return nil, errors.New("no model given")
		}
		fg := p.fromShell(ctx)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.asking {
			return nil, errors.New("the model is switched by the user, not by the assistant")
		}
		if fg != nil {
			return nil, fg
		}
		return p.switchModel(mp)
	}),
	rpc.MethodApplyConfig: decoded(func(p *Proxy, ctx context.Context, ap rpc.AgentParams) (any, error) {
		return p.applyConfig(ctx, ap)
	}),
	rpc.MethodConfig: decoded(func(p *Proxy, ctx context.Context, cp rpc.ConfigParams) (any, error) {
		return p.configFor(ctx, cp)
	}),
	rpc.MethodModels: decoded(func(p *Proxy, ctx context.Context, mp rpc.ModelsParams) (any, error) {
		return p.models(ctx, mp)
	}),
	rpc.MethodPolicy: decoded(func(p *Proxy, ctx context.Context, pp rpc.PolicyParams) (any, error) {
		return p.checkPolicy(ctx, pp)
	}),
	rpc.MethodStatus: func(p *Proxy, _ context.Context, _ json.RawMessage) (any, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.status(), nil
	},
	rpc.MethodHistory: func(p *Proxy, _ context.Context, _ json.RawMessage) (any, error) {
		return p.session().Entries(), nil
	},
	rpc.MethodAgentStart: decoded(func(p *Proxy, ctx context.Context, ap rpc.AgentParams) (any, error) {
		return nil, p.agentStart(ctx, ap)
	}),
	rpc.MethodAgentResume: decoded(func(p *Proxy, ctx context.Context, ap rpc.AgentParams) (any, error) {
		return nil, p.agentResume(ctx, ap)
	}),
	rpc.MethodCompact: decoded(func(p *Proxy, ctx context.Context, ap rpc.AgentParams) (any, error) {
		return nil, p.compact(ctx, ap)
	}),
	rpc.MethodRecap: decoded(func(p *Proxy, ctx context.Context, ap rpc.AgentParams) (any, error) {
		return nil, p.recap(ctx, ap)
	}),
	rpc.MethodAgentCancel: func(p *Proxy, _ context.Context, _ json.RawMessage) (any, error) {
		p.cancelRequest()
		return nil, nil
	},
	rpc.MethodFolds: func(p *Proxy, _ context.Context, _ json.RawMessage) (any, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return append([]Fold{}, p.folds...), nil
	},
	rpc.MethodTasks: func(p *Proxy, _ context.Context, params json.RawMessage) (any, error) {
		return p.tasks(params)
	},
	rpc.MethodClear: func(p *Proxy, ctx context.Context, params json.RawMessage) (any, error) {
		var cp rpc.ClearParams
		if len(params) > 0 { // none at all: plain `aish clear`
			if err := json.Unmarshal(params, &cp); err != nil {
				return nil, err
			}
		}
		return p.clear(ctx, cp)
	},
	rpc.MethodResume: decoded(func(p *Proxy, ctx context.Context, rp rpc.ResumeParams) (any, error) {
		return p.resume(ctx, rp.ID)
	}),
	rpc.MethodMCPStatus: func(p *Proxy, _ context.Context, _ json.RawMessage) (any, error) {
		return p.mcp.Status(), nil
	},
	rpc.MethodMCPList: decoded(func(p *Proxy, ctx context.Context, lp mcp.ListParams) (any, error) {
		return p.mcp.List(ctx, lp.Wait), nil
	}),
	rpc.MethodMCPCall: decoded(func(p *Proxy, ctx context.Context, cp mcp.CallParams) (any, error) {
		return p.mcp.Call(ctx, cp.Name, cp.Args)
	}),
}
