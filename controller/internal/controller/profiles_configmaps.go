package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/pangjian-pj/KubeMorph/controller/internal/optimizer"
)

const (
	defaultProfilesNamespace   = "kubex-system"
	configMapInstanceCostName  = "instance-cost-profiles"
	configMapFamilyEnergyName  = "instance-family-energy-profiles"
	configMapRegionLatencyName = "region-latency-matrix"
	configMapRegionLatencyKey  = "matrix.yaml"
)

type instanceCostProfile struct {
	Family    string `json:"family"`
	Resources struct {
		CPU int `json:"cpu"`
	} `json:"resources"`
	Cost struct {
		Price float64 `json:"price"`
	} `json:"cost"`
}

type familyEnergyProfile struct {
	BaseCores int `json:"baseCores"`
	Energy    struct {
		PowerSamples []struct {
			Util  float64 `json:"util"`
			Power float64 `json:"power"`
		} `json:"powerSamples"`
	} `json:"energy"`
}

type profilesData struct {
	InstancePrice map[string]float64
	InstancePower map[string]optimizer.PowerCurve
	// CityRegionLatencyMs is a matrix: sourceCity -> region -> latencyMs
	CityRegionLatencyMs map[string]map[string]float64
	// RegionLatencyMs is a matrix: regionA -> regionB -> latencyMs.
	// It is used by Communication plugin.
	RegionLatencyMs map[string]map[string]float64
}

type topologyTemplate struct {
	Nodes     []string `json:"nodes"`
	Adjacency [][]int  `json:"adjacency"`
}

// loadTopologyFromConfigMap reads a user-selected topology template from a ConfigMap.
// Contract:
// - cmName is the ConfigMap name in the given namespace.
// - cm.Data contains one key whose value is a JSON object: {nodes:[...], adjacency:[[...]]}.
// - Returns Dependencies: service -> dependent services.
func loadTopologyFromConfigMap(ctx context.Context, c client.Client, namespace, cmName string) (map[optimizer.NamespacedName][]optimizer.NamespacedName, error) {
	if cmName == "" {
		return nil, fmt.Errorf("topologyRef is empty")
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, &cm); err != nil {
		return nil, err
	}
	if len(cm.Data) == 0 {
		return nil, fmt.Errorf("configmap %s/%s has empty data", namespace, cmName)
	}
	// Pick a deterministic key: prefer a key equal to cmName; otherwise the first key.
	key := ""
	if _, ok := cm.Data[cmName]; ok {
		key = cmName
	} else {
		for k := range cm.Data {
			key = k
			break
		}
	}
	raw := cm.Data[key]
	var tpl topologyTemplate
	if err := json.Unmarshal([]byte(raw), &tpl); err != nil {
		return nil, fmt.Errorf("parse %s/%s[%s]: %w", namespace, cmName, key, err)
	}
	if len(tpl.Nodes) == 0 {
		return nil, fmt.Errorf("invalid topology %s/%s[%s]: nodes is empty", namespace, cmName, key)
	}
	if len(tpl.Adjacency) != len(tpl.Nodes) {
		return nil, fmt.Errorf("invalid topology %s/%s[%s]: adjacency rows=%d nodes=%d", namespace, cmName, key, len(tpl.Adjacency), len(tpl.Nodes))
	}
	for i := range tpl.Adjacency {
		if len(tpl.Adjacency[i]) != len(tpl.Nodes) {
			return nil, fmt.Errorf("invalid topology %s/%s[%s]: adjacency row %d len=%d nodes=%d", namespace, cmName, key, i, len(tpl.Adjacency[i]), len(tpl.Nodes))
		}
	}

	deps := make(map[optimizer.NamespacedName][]optimizer.NamespacedName, len(tpl.Nodes))
	idx := make(map[string]int, len(tpl.Nodes))
	for i, n := range tpl.Nodes {
		idx[n] = i
	}
	for i, svc := range tpl.Nodes {
		from := optimizer.NamespacedName{Namespace: "", Name: svc}
		row := tpl.Adjacency[i]
		out := make([]optimizer.NamespacedName, 0)
		for j, v := range row {
			if v == 0 {
				continue
			}
			to := optimizer.NamespacedName{Namespace: "", Name: tpl.Nodes[j]}
			out = append(out, to)
		}
		deps[from] = out
	}
	return deps, nil
}

type regionLatencyMatrix struct {
	Regions []string                      `json:"regions"`
	Latency map[string]map[string]float64 `json:"latency"`
}

func loadProfilesFromConfigMaps(ctx context.Context, c client.Client, namespace string) (*profilesData, error) {
	if namespace == "" {
		namespace = defaultProfilesNamespace
	}

	var cmCost corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: configMapInstanceCostName}, &cmCost); err != nil {
		return nil, err
	}
	var cmEnergy corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: configMapFamilyEnergyName}, &cmEnergy); err != nil {
		return nil, err
	}
	var cmLatency corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: configMapRegionLatencyName}, &cmLatency); err != nil {
		return nil, err
	}

	price := make(map[string]float64, len(cmCost.Data))
	// family -> (baseCores, curve)
	familyProfiles := make(map[string]familyEnergyProfile, len(cmEnergy.Data))
	for k, raw := range cmEnergy.Data {
		var fp familyEnergyProfile
		if err := yaml.Unmarshal([]byte(raw), &fp); err != nil {
			return nil, fmt.Errorf("parse %s/%s[%s]: %w", namespace, configMapFamilyEnergyName, k, err)
		}
		familyProfiles[k] = fp
	}

	instancePower := make(map[string]optimizer.PowerCurve, len(cmCost.Data))
	for inst, raw := range cmCost.Data {
		var p instanceCostProfile
		if err := yaml.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("parse %s/%s[%s]: %w", namespace, configMapInstanceCostName, inst, err)
		}
		price[inst] = p.Cost.Price

		if p.Family == "" || p.Resources.CPU <= 0 {
			continue
		}
		fp, ok := familyProfiles[p.Family]
		if !ok {
			continue
		}
		if fp.BaseCores <= 0 || len(fp.Energy.PowerSamples) < 2 {
			continue
		}
		scale := float64(p.Resources.CPU) / float64(fp.BaseCores)
		samples := make([]optimizer.PowerSample, 0, len(fp.Energy.PowerSamples))
		for _, s := range fp.Energy.PowerSamples {
			samples = append(samples, optimizer.PowerSample{Util: s.Util, Power: s.Power * scale})
		}
		instancePower[inst] = optimizer.PowerCurve{Samples: samples}
	}

	// Parse latency matrix.
	var cityRegionLatency map[string]map[string]float64
	var regionRegionLatency map[string]map[string]float64
	if raw, ok := cmLatency.Data[configMapRegionLatencyKey]; ok && raw != "" {
		var m regionLatencyMatrix
		if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("parse %s/%s[%s]: %w", namespace, configMapRegionLatencyName, configMapRegionLatencyKey, err)
		}
		// Keep only regions defined in the authoritative list; normalize missing entries to absence (plugin will error).
		allowed := map[string]struct{}{}
		for _, r := range m.Regions {
			if r != "" {
				allowed[r] = struct{}{}
			}
		}
		// Keep the raw region->region latency matrix for Communication.
		regionRegionLatency = make(map[string]map[string]float64, len(m.Latency))
		for src, row := range m.Latency {
			if src == "" {
				continue
			}
			// Only keep src that is in authoritative regions.
			if _, ok := allowed[src]; !ok {
				continue
			}
			outRow := make(map[string]float64, len(row))
			for dst, lat := range row {
				if _, ok := allowed[dst]; !ok {
					continue
				}
				outRow[dst] = lat
			}
			regionRegionLatency[src] = outRow
		}

		// For Latency plugin we reuse the same matrix as city->region (sourceCity -> region).
		// This is a simplification in early milestones: "city" keys share the same namespace as regions.
		cityRegionLatency = make(map[string]map[string]float64, len(regionRegionLatency))
		for src, row := range regionRegionLatency {
			cityRegionLatency[src] = row
		}
	}

	return &profilesData{InstancePrice: price, InstancePower: instancePower, CityRegionLatencyMs: cityRegionLatency, RegionLatencyMs: regionRegionLatency}, nil
}
