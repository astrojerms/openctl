package base

#Resource: {
	apiVersion: =~"^[a-z0-9]+\\.openctl\\.io/v[0-9]+.*$"
	kind:       string
	metadata:   #Metadata
	spec?:      _
	status?:    _
}

#Metadata: {
	name:         =~"^[a-z0-9][a-z0-9-]*[a-z0-9]$" | =~"^[a-z0-9]$"
	namespace?:   string
	labels?:      {[string]: string}
	annotations?: {[string]: string}
}

#IPv4: =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}$"
#CIDR: =~"^([0-9]{1,3}\\.){3}[0-9]{1,3}/[0-9]{1,2}$"
