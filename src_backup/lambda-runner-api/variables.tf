variable "region" {
  type = string
  default = "ap-southeast-1"
}

variable "github-token" {
    type = string
    default = "your-github-access-token"
    description = "Your PAT with runner access permissions"
}


# Example your org link is: https://github.com/my-org-name
# => value must be: my-org-name
variable "github-org" {
    type = string
    default = "your-github-org"
    description = "Your GitHub organization"
}