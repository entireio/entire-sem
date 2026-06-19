function graphqlFetch() {
  return gql`
    query GetUser {
      user {
        id
      }
    }
  `;
}

function makeRouter() {
  return router({
    user: publicProcedure.query(() => {
      return "ok";
    }),
  });
}
