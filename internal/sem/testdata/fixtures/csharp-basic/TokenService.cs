using System;

namespace Auth
{
    public class TokenService
    {
        public bool Validate(string token)
        {
            return !String.IsNullOrEmpty(token);
        }

        public bool Check(string token)
        {
            return Validate(token);
        }
    }
}
