package v1alpha1

// RepositorySelector picks repositories by a doublestar pattern.
type RepositorySelector struct {
	// Decoration says whether the pattern includes or excludes.
	// +kubebuilder:validation:Enum=repoMatches;repoExcludes
	Decoration string `json:"decoration"`
	// Pattern is a doublestar pattern, for example `**`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Pattern string `json:"pattern"`
}

// TagSelector picks tags by a doublestar pattern. It is used by retention rules.
type TagSelector struct {
	// Decoration says whether the pattern includes or excludes.
	// +kubebuilder:validation:Enum=matches;excludes
	Decoration string `json:"decoration"`
	// Pattern is a doublestar pattern, for example `**`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Pattern string `json:"pattern"`
	// Untagged also selects artifacts that have no tag.
	// +optional
	Untagged bool `json:"untagged,omitempty"`
}

// ImmutabilityTagSelector picks tags by a doublestar pattern. It is used by immutability rules, which have no untagged option.
type ImmutabilityTagSelector struct {
	// Decoration says whether the pattern includes or excludes.
	// +kubebuilder:validation:Enum=matches;excludes
	Decoration string `json:"decoration"`
	// Pattern is a doublestar pattern, for example `**`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Pattern string `json:"pattern"`
}
