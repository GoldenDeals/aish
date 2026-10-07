package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GoldenDeals/aish/internal/mcp"
	"github.com/GoldenDeals/aish/internal/rpc"
)

func (p *Proxy) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case rpc.MethodInfo:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.info(), nil
	case rpc.MethodModel:
		var mp rpc.ModelParams
		if err := json.Unmarshal(params, &mp); err != nil {
			return nil, err
		}
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
	case rpc.MethodApplyConfig:
		var ap rpc.AgentParams
		if err := json.Unmarshal(params, &ap); err != nil {
			return nil, err
		}
		return p.applyConfig(ctx, ap)
	case rpc.MethodConfig:
		var cp rpc.ConfigParams
		if err := json.Unmarshal(params, &cp); err != nil {
			return nil, err
		}
		return p.configFor(ctx, cp)
	case rpc.MethodModels:
		var mp rpc.ModelsParams
		if err := json.Unmarshal(params, &mp); err != nil {
			return nil, err
		}
		return p.models(ctx, mp)
	case rpc.MethodPolicy:
		var pp rpc.PolicyParams
		if err := json.Unmarshal(params, &pp); err != nil {
			return nil, err
		}
		return p.checkPolicy(ctx, pp)
	case rpc.MethodStatus:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.status(), nil
	case rpc.MethodHistory:
		return p.session().Entries(), nil
	case rpc.MethodAgentStart, rpc.MethodAgentResume, rpc.MethodCompact:
		var ap rpc.AgentParams
		if err := json.Unmarshal(params, &ap); err != nil {
			return nil, err
		}
		switch method {
		case rpc.MethodAgentStart:
			return nil, p.agentStart(ctx, ap)
		case rpc.MethodAgentResume:
			return nil, p.agentResume(ctx, ap)
		}
		return nil, p.compact(ctx, ap)
	case rpc.MethodAgentCancel:
		p.cancelRequest()
		return nil, nil
	case rpc.MethodFolds:
		p.mu.Lock()
		defer p.mu.Unlock()
		return append([]Fold{}, p.folds...), nil
	case rpc.MethodTasks:
		return p.tasks(params)
	case rpc.MethodClear:
		var cp rpc.ClearParams
		if len(params) > 0 { // none at all: plain `aish clear`
			if err := json.Unmarshal(params, &cp); err != nil {
				return nil, err
			}
		}
		return p.clear(ctx, cp)
	case rpc.MethodResume:
		var rp rpc.ResumeParams
		if err := json.Unmarshal(params, &rp); err != nil {
			return nil, err
		}
		return p.resume(ctx, rp.ID)
	case rpc.MethodMCPStatus:
		return p.mcp.Status(), nil
	case rpc.MethodMCPList:
		var lp mcp.ListParams
		if err := json.Unmarshal(params, &lp); err != nil {
			return nil, err
		}
		return p.mcp.List(ctx, lp.Wait), nil
	case rpc.MethodMCPCall:
		var cp mcp.CallParams
		if err := json.Unmarshal(params, &cp); err != nil {
			return nil, err
		}
		return p.mcp.Call(ctx, cp.Name, cp.Args)
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
